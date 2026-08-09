# Tempo cache-backed HTTP handler

Status: approved 2026-08-10

## Purpose

Serve the Tempo colour of a single day over HTTP without calling the RTE API on
every request. RTE rate-limits the calendar endpoint, but Tempo history is
immutable once published, so almost every question can be answered from a cache.
The handler fills that cache in wide sweeps — 360 days per upstream call — so a
cold cache warms in a handful of requests rather than one day at a time.

## Scope

In scope: a `Store` interface with filesystem and S3 implementations, a
coordinator that reads the cache and fills it under a rate limit, and an
`http.Handler` exposing one day per URL. Out of scope: range queries, a
write API, authentication of the handler's own callers, and metrics.

Everything lives in package `tempo` (`domains/tempo`), alongside the existing
client. New dependencies in the root `go.mod`: `aws-sdk-go-v2` (S3) and
`golang.org/x/time/rate`.

```
domains/tempo/
  date.go        Date value type and Paris-calendar arithmetic
  store.go       Store interface, entry format, floor marker
  store_file.go  FileStore  (root/YYYY/MM/DD.json)
  store_s3.go    S3Store    (bucket/prefix/YYYY/MM/DD.json)
  window.go      window planning (centre, shift, trim)
  cache.go       Cache: coordination, rate limiting, persistence
  handler.go     Handler: http.Handler over Cache
```

## Definitions

**Tempo day.** A day runs from Paris local midnight to Paris local midnight. All
calendar arithmetic is in `Europe/Paris`, resolved once at package level the way
`xml.go` already does it; a binary on a system without the zone database needs
`import _ "time/tzdata"`.

**Horizon.** The latest day the handler will serve: today, or tomorrow once the
Paris local time is 11:00 or later. RTE publishes the next day's colour in the
late morning; before 11:00 there is nothing to serve.

**Floor.** The earliest day RTE holds data for. Learned from responses, not
configured. Until it is learned there is no lower bound.

## Date

`Date` is a Paris calendar day, deliberately not a `time.Time`. The cache key,
the window arithmetic and the URL path are all calendar-day concepts; using an
instant for them invites off-by-one-hour errors at the DST boundary that the
existing `Day` documentation already warns about.

```go
type Date struct {
    Year  int
    Month time.Month
    Day   int
}

func (d Date) Key() string                                  // "2026/08/10.json"
func (d Date) Add(days int) Date
func (d Date) Compare(o Date) int
func (d Date) Range(loc *time.Location) (start, end time.Time)
```

## Store

```go
// Store persists one JSON object per Tempo day.
type Store interface {
    Get(ctx context.Context, key string) ([]byte, error)
    Put(ctx context.Context, key string, data []byte) error
    List(ctx context.Context, prefix string) ([]string, error)
}

// ErrNotCached is returned by Get when the key is absent.
var ErrNotCached = errors.New("tempo: not cached")
```

`List` exists for S3. Planning a window needs to know which of 360 days are
already cached; probing that with 360 `HEAD` requests would cost more than the
API call it saves. Listing the at most 13 month prefixes a window spans is one
call each — `ReadDir` on the filesystem, `ListObjectsV2` on S3 — and the result
is held in a small in-memory index for the life of the process.

`FileStore` writes through a temp file and renames it into place, so a crashed
write never leaves a truncated entry to be read as valid.

`S3Store` takes a narrow interface with one method per operation rather than
`*s3.Client`, so it can be tested with a fake and the AWS types stay at the edge
of the package.

## Entry format

```jsonc
// 2026/08/10.json — a published day
{"start":"2026-08-10T00:00:00+02:00","end":"2026-08-11T00:00:00+02:00",
 "colour":"BLUE","updated_at":"2026-08-09T10:32:00+02:00"}

// 2019/03/07.json — a hole inside the available history
{"absent":true}

// floor.json — the bottom of RTE's history
{"earliest":"2014-09-01"}
```

There is no `fallback` field. A degraded-mode value is provisional and may still
change, so it is served from the fetch and never written. The invariant is that
**everything in the cache is final**, which is what allows entries to be served
without any expiry check.

## Window planning

A pure function, so it can be tested exhaustively without a store or a server:

```go
func planWindow(d, floor, horizon Date, cached func(Date) bool, size int) (lo, hi Date, ok bool)
```

1. **Centre** — 180 days either side of `d`, 360 days in total.
2. **Shift** — if `hi` overruns the horizon, slide the whole window back so it
   still holds 360 days. If `lo` then falls below the floor, clamp it and accept
   a shorter window.
3. **Trim** — remove the leading run of already-cached days from `lo` and the
   trailing run from `hi`. Cached days in the *interior* stay inside the range
   and are re-fetched for free, since the cost is one call either way. An
   `absent` marker counts as cached, so a known hole trims like any other day.
4. `ok` is false when nothing remains, and no upstream call is made.

Because step 2 shifts rather than extends, and step 3 only narrows, the range
can never exceed 366 days and so can never provoke `TMPLIKSUPCON_TMPLIKCAL_F03`.

## Cache

```go
type Cache struct {
    Service *Service       // upstream RTE client
    Store   Store
    Now     func() time.Time
    Limiter *rate.Limiter  // default 1/s, burst 2
    Window  int            // default 360
}

func (c *Cache) Day(ctx context.Context, d Date) (Day, error)
```

Errors: `ErrBeyondHorizon` for a day past the horizon, `ErrNoData` for a day
below the floor or explicitly absent. Upstream failures come back as
`*rtehttp.APIError` unchanged.

Flow:

- A day past the horizon or below the learned floor is rejected without
  touching the network.
- **Hit.** The entry answers the request directly; `absent` answers `ErrNoData`.
  If `planWindow` then reports uncached territory nearby, a fill starts on a
  detached context and is *not* waited for, so the response is never delayed by
  cache maintenance.
- **Miss.** The request joins the in-flight fill whose range already covers `d`,
  or starts one, and waits on that fill's channel against its own context. If
  the context expires first the fill continues for the next caller.

The answer to a miss is taken from the fetch result, not from a re-read of the
store. This is what lets a fallback day be served while still never being
written: a re-read would miss and loop.

One fill performs:

```
limiter.Wait(ctx)  →  Service.Calendars(ctx, window)  →  persist  →  close(done)
```

Deduplication is by *coverage*, not by exact window key. Concurrent misses for
`D` and `D+1` plan windows one day apart; matching on containment collapses them
into a single upstream call rather than two. This is the property that protects
the rate limit under load.

Persisting a fetch of `[lo, hi]`:

- each returned non-fallback day becomes an entry;
- each returned fallback day is written nowhere, so the next request refetches
  it;
- each day in `[lo, hi]` that the response did not mention at all is written as
  `{"absent":true}`;
- except a *leading* gap — a response starting later than `lo` means the window
  reached the bottom of RTE's history, so `floor.json` records the earliest
  returned day instead of writing thousands of absent markers. Gaps inside the
  response are ordinary holes and get per-day markers.

## Handler

```go
type Handler struct{ Cache *Cache }
```

`GET /YYYY/MM/DD`, mountable under any prefix with `http.StripPrefix`. The path
mirrors the cache layout.

```json
{"date":"2026-08-10","colour":"BLUE",
 "start":"2026-08-10T00:00:00+02:00","end":"2026-08-11T00:00:00+02:00"}
```

`"fallback":true` is added when the value is provisional, so a client can tell
that it may still change.

| outcome | status |
| --- | --- |
| day served | 200 |
| beyond the horizon | 400 |
| malformed or impossible date (`2026/02/30`) | 400 |
| below the floor, or an absent day | 404 |
| upstream RTE failure | 502 |
| wait exceeded the request context | 504 |
| method other than GET or HEAD | 405 |

Error bodies are plain text. A day strictly in the past is served with
`Cache-Control: public, max-age=31536000, immutable`, since a published past day
never changes; today and tomorrow get a short max-age.

## Testing

Window planning and the horizon rule are pure functions covered by table tests,
including 10:59 against 11:00 Paris time and both DST switchovers.

`Cache` is tested against an in-memory `Store` and an `httptest` upstream:

- a hit answers without any upstream call;
- a miss fills and answers;
- a fallback day is served but never written;
- a leading gap writes `floor.json` and later requests below it make no call;
- an interior gap writes per-day `absent` markers;
- N simultaneous misses for nearby days produce exactly one upstream call;
- a request whose context expires returns while the fill continues.

`FileStore` runs against `t.TempDir()`, including the crash-safety of the
rename. `S3Store` runs against a fake of the narrow S3 interface. The handler
has a table test over the status mapping.

The whole suite runs offline: `go test -race -cover ./...`.
