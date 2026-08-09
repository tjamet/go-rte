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
domains/tempo   the Tempo Like Supply Contract API
```

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
