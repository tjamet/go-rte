package tempo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Errors a Cache returns about the day itself, as opposed to a transport or
// store failure.
var (
	// ErrBeyondHorizon means the day is later than anything RTE has published:
	// after today, or after tomorrow once Paris has passed the publication hour.
	ErrBeyondHorizon = errors.New("tempo: day is beyond what has been published")
	// ErrNoData means RTE has no colour for the day — it is before the archive
	// begins, or it is a hole inside it.
	ErrNoData = errors.New("tempo: no data for this day")
)

// Defaults for the rate limiter that gates every call to RTE.
const (
	DefaultRate  = rate.Limit(1)
	DefaultBurst = 2
)

// maxBackgroundFills caps the fills started to widen the cache behind a hit.
// They are best-effort, so declining to start another when several are already
// waiting on the limiter is better than letting a burst of requests for distant
// days accumulate goroutines.
const maxBackgroundFills = 4

// Cache answers for one Tempo day, reading from a Store and filling it from RTE
// in wide sweeps.
//
// Every day it holds is final — degraded-mode values are never written — so a
// cached answer is served without any expiry check.
type Cache struct {
	// Service reaches the RTE calendar endpoint. Required.
	Service *Service
	// Store persists the days. Required.
	Store Store
	// Now supplies the current instant; defaults to time.Now. Set it in tests to
	// fix which day counts as today.
	Now func() time.Time
	// Limiter gates every call to RTE; defaults to DefaultRate and DefaultBurst.
	Limiter *rate.Limiter
	// WindowDays is how many days one fetch asks for; defaults to
	// DefaultWindowDays.
	WindowDays int
	// FillTimeout bounds a fill that outlives the request that started it;
	// defaults to DefaultFillTimeout.
	FillTimeout time.Duration

	locOnce  sync.Once
	location *time.Location
	locErr   error

	limiterOnce sync.Once
	fallbackRPS *rate.Limiter

	mu sync.Mutex
	// held is the set of days known to be in the store, so planning a window
	// does not have to ask the store about each of its 360 days.
	held map[Date]bool
	// listed records the month prefixes already read into held.
	listed map[string]bool
	// floor is the earliest day RTE has data for, once learned.
	floor       Date
	floorLoaded bool
	// fills are the fetches in flight, matched by coverage so that a request can
	// join one that already covers its day.
	fills []*fill
}

// DefaultFillTimeout bounds a fill once no caller is waiting on it.
const DefaultFillTimeout = 2 * time.Minute

// fill is one in-flight fetch, shared by every request its window covers.
type fill struct {
	window window
	done   chan struct{}
	days   map[Date]Day
	err    error
}

// Day returns the colour of d.
//
// A day past the horizon or below the learned floor is answered without
// touching the network. Otherwise the store is consulted, and a miss joins or
// starts the single fetch covering d and waits for it. If ctx expires first the
// fetch carries on, so the next caller finds the cache warm.
func (c *Cache) Day(ctx context.Context, d Date) (Day, error) {
	loc, err := c.loc()
	if err != nil {
		return Day{}, err
	}
	if horizon := horizonAt(c.now(), loc); d.After(horizon) {
		return Day{}, fmt.Errorf("tempo: %s is after %s: %w", d, horizon, ErrBeyondHorizon)
	}
	floor, err := c.knownFloor(ctx)
	if err != nil {
		return Day{}, err
	}
	if !floor.IsZero() && d.Before(floor) {
		return Day{}, fmt.Errorf("tempo: %s is before %s: %w", d, floor, ErrNoData)
	}

	cached, err := c.lookup(ctx, d)
	if err != nil {
		return Day{}, err
	}
	if cached != nil {
		c.widen(d)
		return cached.answer(d)
	}

	days, err := c.fetch(ctx, d)
	if err != nil {
		return Day{}, err
	}
	if day, ok := days[d]; ok {
		return day, nil
	}
	return Day{}, fmt.Errorf("tempo: %s: %w", d, ErrNoData)
}

// answer turns a cache entry into the result for d.
func (e *entry) answer(d Date) (Day, error) {
	if e.Absent {
		return Day{}, fmt.Errorf("tempo: %s: %w", d, ErrNoData)
	}
	return e.day(), nil
}

// lookup reads d from the store, returning nil when it is not held.
func (c *Cache) lookup(ctx context.Context, d Date) (*entry, error) {
	data, err := c.Store.Get(ctx, d.Key())
	if errors.Is(err, ErrNotCached) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoded, err := decodeEntry(data)
	if err != nil {
		return nil, err
	}
	c.remember(d)
	return &decoded, nil
}

// fetch resolves d by joining or starting the fill that covers it, then waiting
// for that fill. It returns the days the fetch produced, which is the source of
// truth rather than the store: a degraded-mode day is answered from here
// precisely because it is never written.
func (c *Cache) fetch(ctx context.Context, d Date) (map[Date]Day, error) {
	f, err := c.fillFor(ctx, d, false)
	if err != nil || f == nil {
		return nil, err
	}
	select {
	case <-f.done:
		return f.days, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// widen starts a fill for the neighbourhood of d without waiting for it, so a
// request served from the cache still extends it. It is best-effort: any error
// is the next request's problem, and nothing is started when several fills are
// already queued behind the limiter.
func (c *Cache) widen(d Date) {
	_, _ = c.fillFor(context.Background(), d, true)
}

// fillFor returns the fill covering d, starting one if none is in flight and
// there is anything left to ask for. It returns nil when the neighbourhood of d
// is already complete.
func (c *Cache) fillFor(ctx context.Context, d Date, background bool) (*fill, error) {
	loc, err := c.loc()
	if err != nil {
		return nil, err
	}
	floor, err := c.knownFloor(ctx)
	if err != nil {
		return nil, err
	}
	horizon := horizonAt(c.now(), loc)

	// Plan twice: once ignoring the cache to learn which months the window could
	// possibly touch, so they can be listed, and once for real.
	widest, _ := planWindow(d, floor, horizon, func(Date) bool { return false }, c.windowDays())
	if err := c.list(ctx, widest); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Coverage, not equality: requests for adjacent days plan windows a day
	// apart, and only matching on coverage collapses them into one call.
	for _, f := range c.fills {
		if f.window.contains(d) {
			return f, nil
		}
	}
	if background && len(c.fills) >= maxBackgroundFills {
		return nil, nil
	}

	w, ok := planWindow(d, floor, horizon, c.isHeldLocked, c.windowDays())
	if !ok {
		return nil, nil
	}

	f := &fill{window: w, done: make(chan struct{})}
	c.fills = append(c.fills, f)
	go c.run(f, loc)
	return f, nil
}

// run performs one fetch and publishes its result to everyone waiting.
//
// It uses its own context rather than the requester's: a fill outlives the
// request that started it, so that a caller giving up does not throw away work
// the next caller needs.
func (c *Cache) run(f *fill, loc *time.Location) {
	ctx, cancel := context.WithTimeout(context.Background(), c.fillTimeout())
	defer cancel()
	defer close(f.done)
	defer c.forget(f)

	if err := c.limiter().Wait(ctx); err != nil {
		f.err = fmt.Errorf("tempo: waiting for the rate limiter: %w", err)
		return
	}
	calendars, err := c.Service.Calendars(ctx, f.window.query(loc))
	if err != nil {
		f.err = err
		return
	}
	f.days, f.err = c.persist(ctx, f.window, calendars, loc)
}

// persist writes what the fetch established and returns the days it found.
//
// Three rules, applied to every day of the window:
//   - a published day is written and returned;
//   - a degraded-mode day is returned but not written, so it is asked about
//     again next time rather than becoming a permanent answer;
//   - a day RTE did not mention is written as absent, so the hole is asked
//     about once — except a leading gap, which means the window ran off the
//     bottom of the archive and is recorded as the floor instead of as
//     thousands of absent days.
func (c *Cache) persist(ctx context.Context, w window, calendars Calendars, loc *time.Location) (map[Date]Day, error) {
	found := make(map[Date]Day, w.days())
	for _, day := range calendars.Days() {
		found[DateOf(day.Start, loc)] = day
	}

	earliest, ok := earliestOf(found)
	if ok && earliest.After(w.Lo) {
		if err := c.recordFloor(ctx, earliest); err != nil {
			return nil, err
		}
	}

	for d := w.Lo; !d.After(w.Hi); d = d.Add(1) {
		if ok && d.Before(earliest) {
			continue // the leading gap, already recorded as the floor
		}
		day, present := found[d]
		if present && day.Fallback {
			continue
		}
		e := absentEntry()
		if present {
			e = entryOf(day)
		}
		if err := c.write(ctx, d.Key(), e); err != nil {
			return nil, err
		}
		c.remember(d)
	}
	return found, nil
}

func earliestOf(days map[Date]Day) (Date, bool) {
	var earliest Date
	for d := range days {
		if earliest.IsZero() || d.Before(earliest) {
			earliest = d
		}
	}
	return earliest, !earliest.IsZero()
}

func (c *Cache) write(ctx context.Context, key string, e entry) error {
	encoded, err := encodeEntry(e)
	if err != nil {
		return err
	}
	return c.Store.Put(ctx, key, encoded)
}

// recordFloor remembers the bottom of the archive, in the store so that a later
// process does not rediscover it, and in memory so this one stops calling out
// for anything below it.
func (c *Cache) recordFloor(ctx context.Context, earliest Date) error {
	encoded, err := json.Marshal(floor{Earliest: earliest})
	if err != nil {
		return fmt.Errorf("tempo: encode floor: %w", err)
	}
	if err := c.Store.Put(ctx, FloorKey, encoded); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.floor, c.floorLoaded = earliest, true
	return nil
}

// knownFloor returns the earliest day RTE has data for, reading it from the
// store the first time. The zero Date means it has not been learned yet.
func (c *Cache) knownFloor(ctx context.Context) (Date, error) {
	c.mu.Lock()
	loaded, known := c.floorLoaded, c.floor
	c.mu.Unlock()
	if loaded {
		return known, nil
	}

	data, err := c.Store.Get(ctx, FloorKey)
	if err != nil && !errors.Is(err, ErrNotCached) {
		return Date{}, err
	}

	var f floor
	if err == nil {
		if err := json.Unmarshal(data, &f); err != nil {
			return Date{}, fmt.Errorf("tempo: decode %s: %w", FloorKey, err)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.floor, c.floorLoaded = f.Earliest, true
	return f.Earliest, nil
}

// list reads the month prefixes w spans into the in-memory set of held days.
// Each month is listed once per process, which is what keeps planning a
// 360-day window down to a handful of store calls instead of 360.
func (c *Cache) list(ctx context.Context, w window) error {
	for month := firstOfMonth(w.Lo); !month.After(w.Hi); month = nextMonth(month) {
		prefix := month.MonthPrefix()

		c.mu.Lock()
		done := c.listed[prefix]
		c.mu.Unlock()
		if done {
			continue
		}

		keys, err := c.Store.List(ctx, prefix)
		if err != nil {
			return err
		}

		c.mu.Lock()
		if c.listed == nil {
			c.listed = map[string]bool{}
		}
		c.listed[prefix] = true
		for _, key := range keys {
			if d, err := dateOfKey(key); err == nil {
				c.rememberLocked(d)
			}
		}
		c.mu.Unlock()
	}
	return nil
}

func firstOfMonth(d Date) Date { return Date{Year: d.Year, Month: d.Month, Day: 1} }

func nextMonth(d Date) Date {
	if d.Month == time.December {
		return Date{Year: d.Year + 1, Month: time.January, Day: 1}
	}
	return Date{Year: d.Year, Month: d.Month + 1, Day: 1}
}

// dateOfKey reads back the day a store key stands for, so listing a month
// populates the set of held days.
func dateOfKey(key string) (Date, error) {
	parts := strings.Split(strings.TrimSuffix(key, ".json"), "/")
	if len(parts) != 3 {
		return Date{}, fmt.Errorf("tempo: %q is not a day key", key)
	}
	return ParseDate(parts[0], parts[1], parts[2])
}

func (c *Cache) remember(d Date) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rememberLocked(d)
}

func (c *Cache) rememberLocked(d Date) {
	if c.held == nil {
		c.held = map[Date]bool{}
	}
	c.held[d] = true
}

func (c *Cache) isHeldLocked(d Date) bool { return c.held[d] }

// forget drops a finished fill so a later miss can start a new one.
func (c *Cache) forget(f *fill) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, candidate := range c.fills {
		if candidate == f {
			c.fills = append(c.fills[:i], c.fills[i+1:]...)
			return
		}
	}
}

// today is the current Paris calendar day. Days before it are settled; today
// and tomorrow are not.
func (c *Cache) today() (Date, error) {
	loc, err := c.loc()
	if err != nil {
		return Date{}, err
	}
	return DateOf(c.now(), loc), nil
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) windowDays() int {
	if c.WindowDays > 0 {
		return c.WindowDays
	}
	return DefaultWindowDays
}

func (c *Cache) fillTimeout() time.Duration {
	if c.FillTimeout > 0 {
		return c.FillTimeout
	}
	return DefaultFillTimeout
}

// limiter gates the calls to RTE. The default is built lazily rather than
// assigned to Limiter, so that a caller reading the field concurrently never
// races with its initialisation.
func (c *Cache) limiter() *rate.Limiter {
	if c.Limiter != nil {
		return c.Limiter
	}
	c.limiterOnce.Do(func() { c.fallbackRPS = rate.NewLimiter(DefaultRate, DefaultBurst) })
	return c.fallbackRPS
}

// loc resolves Europe/Paris once. Every Tempo day is anchored there, so a
// system without the zone database cannot serve any of them.
func (c *Cache) loc() (*time.Location, error) {
	c.locOnce.Do(func() {
		c.location, c.locErr = time.LoadLocation(parisZone)
		if c.locErr != nil {
			c.locErr = fmt.Errorf(
				`tempo: load %s (import _ "time/tzdata" to embed the zone database): %w`, parisZone, c.locErr)
		}
	})
	return c.location, c.locErr
}
