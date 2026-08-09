# go-rte

A Go client for the [RTE France](https://data.rte-france.com) open data platform.

Currently implements the **Tempo Like Supply Contract** API: the colour
(BLUE / WHITE / RED) assigned to each day of a Tempo tariff year. A year holds
20 red days, 40 white days, and blue days for the rest; RTE publishes the next
day's colour every afternoon.

```go
import (
	"github.com/tjamet/go-rte/domains/tempo"
	"github.com/tjamet/go-rte/rte"
)

client := rte.New(rte.Options{
	ClientID:     os.Getenv("RTE_CLIENT_ID"),
	ClientSecret: os.Getenv("RTE_CLIENT_SECRET"),
})

calendars, err := client.Tempo.Calendars(ctx, tempo.Query{})
if err != nil {
	return err
}
for _, day := range calendars.Days() {
	fmt.Printf("%s %s\n", day.Start.Format(time.DateOnly), day.Colour)
}
```

## Credentials

Register an application on the [RTE portal](https://data.rte-france.com),
subscribe it to the APIs you need, and use the generated id and secret. The
client exchanges them for a bearer token at
`https://digital.iservices.rte-france.com/token/oauth` and refreshes it as
needed — you never handle the token.

## Selecting a window

`Query` takes an optional date range and a fallback (degraded mode) filter.
Start and end must be given together or not at all; the client rejects a
half-specified range before making a request.

```go
calendars, err := client.Tempo.Calendars(ctx, tempo.Query{
	Start: time.Date(2026, 1, 1, 0, 0, 0, 0, paris),
	End:   time.Date(2026, 8, 11, 0, 0, 0, 0, paris),
})
```

Days come back oldest first from `Calendars.Days()`, and `ColourAt` answers the
common question directly:

```go
colour, ok := calendars.ColourAt(time.Now())
```

Timestamps keep the UTC offset RTE sent rather than being normalised to UTC,
because a Tempo day runs from Paris local midnight to Paris local midnight.

## Errors

RTE reports every business failure of the calendar endpoint as HTTP 400, so the
error *code* — not the status — is what distinguishes them:

```go
if rtehttp.Code(err) == tempo.CodePeriodTooLong {
	// the window exceeds what one call may return: split it and retry
}
```

Failures come back as `*rtehttp.APIError`, carrying the code, the description
and the `TransactionID` that RTE support asks for. The two failures decidable
without calling out — a half-specified and an inverted range — are returned as
`tempo.ErrDateRangeIncomplete` and `tempo.ErrDateRangeInverted`, usable with
`errors.Is`.

## Serving days from a cache

Tempo history is immutable once published, so almost every question can be
answered without calling RTE. `tempo.Cache` reads a `tempo.Store` and fills it
from the API in 360-day sweeps; `tempo.Handler` puts one day per URL in front
of it.

```go
cache := &tempo.Cache{
	Service: client.Tempo,
	Store:   &tempo.FileStore{Root: "/var/cache/tempo"},
}
mux.Handle("/tempo/", http.StripPrefix("/tempo", &tempo.Handler{Cache: cache}))
```

```
GET /tempo/2026/08/10

{"date":"2026-08-10","colour":"BLUE",
 "start":"2026-08-10T00:00:00+02:00","end":"2026-08-11T00:00:00+02:00"}
```

The latest day served is today, or tomorrow once Paris has passed 11:00, which
is when RTE publishes it. Beyond that is `400`: the day does not exist yet.
Before the archive begins, or a hole inside it, is `404`. An upstream failure is
`502`, and giving up while waiting for a fetch is `504`.

Substitute `tempo.S3Store` to share one cache between processes:

```go
Store: &tempo.S3Store{Client: s3.NewFromConfig(cfg), Bucket: "tempo", Prefix: "cache"},
```

### Why the window is 360 days wide

The rate limit counts calls, not days, so asking for a single day is the worst
possible use of one. A miss centres 360 days on the day requested, slides the
range back if it would reach past what has been published — a short call costs
the same as a full one — and trims the already-cached runs off each end. Cached
days in the *middle* stay in the range, because re-fetching them is free while
splitting the range around them would spend a second call.

Concurrent misses are collapsed by coverage rather than by exact range, so
twenty requests for neighbouring days cost one call rather than twenty, and a
token bucket gates whatever survives that. A request that gives up does not
cancel the fetch it was waiting on: the next caller finds the cache warm.

A day served from cache is also answered immediately and *then* used to widen
the cache in the background, so a caller walking through history keeps finding
the days it is about to ask for.

### What is never cached

Degraded-mode values. RTE marks a provisional colour with `fallback`, and it may
still be revised, so it is served but never written — the handler returns
`"fallback":true` with `Cache-Control: no-store`, and the day is fetched again
next time. Everything the cache does hold is final, which is why entries have no
expiry. A published past day is served `immutable`; today and tomorrow get a
short max-age.

Absence is cached too, as `{"absent":true}`, so a hole in the archive is asked
about once rather than on every request. Pre-history is recorded as a single
`floor.json` instead of thousands of absent days, and it survives a restart, so
a fresh process does not rediscover it.

## Authentication seam

Everything reaches RTE through an `auth.Authenticator`:

```go
type Authenticator interface {
	Authenticate(r *http.Request) error
}
```

`rte.New` uses the OAuth2 client-credentials implementation. If you already
hold a token, or authenticate some other way, supply your own:

```go
client := rte.NewWithAuthenticator(auth.StaticToken(myToken), rte.Options{})
```

## Layout

```
rte/            facade wiring authenticator + HTTP client + domain services
shared/auth     Authenticator, StaticToken, ClientCredentials, Transport
shared/rtehttp  shared HTTP plumbing and typed APIError
domains/tempo   the Tempo Like Supply Contract API, its cache and its handler
```

Within `domains/tempo`, `service.go` and `rest.go` reach the API, `date.go`
and `window.go` decide which days to ask for, `store*.go` persist them, and
`cache.go` and `handler.go` serve them.

Further RTE APIs become new packages under `domains/` plus a field on
`rte.Client`; the auth and HTTP layers are already shared.

## A note on the response shape

`Swagger.json`, RTE's own sample archive, and the live API describe the calendar
response three mutually incompatible ways:

| source | `tempo_like_calendars` | `values` | colour key | `fallback` |
| --- | --- | --- | --- | --- |
| `Swagger.json` | array | array | `values` | `"true"` (string) |
| sample archive (2016) | array | **object** | `value` | `true` (bool) |
| live API (2026-08) | **object** | array | `value` | absent |

Decoding accepts all three, because a client written against any one of them
silently yields zero values against the other two. The endpoint can also answer
in a legacy XML dialect (French element and colour names, date-only values,
inconsistent root element); that is decoded into the same types.

XML decoding resolves dates against `Europe/Paris`. On a system without the
zone database, add `import _ "time/tzdata"` to your binary.

## Testing

```
go test -race -cover ./...
```

The suite runs entirely against fakes. To exercise the real platform:

```
RTE_CLIENT_ID=… RTE_CLIENT_SECRET=… go test -tags=live -run TestLive ./rte/...
```
