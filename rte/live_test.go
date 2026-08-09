//go:build live

// This test talks to the real RTE platform. It is excluded from the normal
// build and only runs with:
//
//	RTE_CLIENT_ID=… RTE_CLIENT_SECRET=… go test -tags=live -run TestLive ./rte/...
//
// It is the check that the fake-backed suite cannot make: that the credentials
// are accepted, that the token endpoint takes the JSON grant body this client
// sends, and that the live payload still decodes.
package rte

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tjamet/go-rte/domains/tempo"
)

func TestLiveCalendars(t *testing.T) {
	id, secret := os.Getenv("RTE_CLIENT_ID"), os.Getenv("RTE_CLIENT_SECRET")
	if id == "" || secret == "" {
		t.Skip("set RTE_CLIENT_ID and RTE_CLIENT_SECRET to run the live test")
	}

	client := New(Options{ClientID: id, ClientSecret: secret})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	end := time.Now().Truncate(24 * time.Hour)
	calendars, err := client.Tempo.Calendars(ctx, tempo.Query{
		Start: end.AddDate(0, 0, -7),
		End:   end,
	})
	require.NoError(t, err)

	days := calendars.Days()
	require.NotEmpty(t, days, "the last seven days must return colours")
	for i, d := range days {
		assert.True(t, d.Colour.Valid(), "day %d has colour %q", i, d.Colour)
		assert.True(t, d.End.After(d.Start), "day %d ends before it starts", i)
		if i > 0 {
			assert.False(t, d.Start.Before(days[i-1].Start), "days must come back oldest first")
		}
	}
	t.Logf("%d days, %s..%s", len(days), days[0].Start, days[len(days)-1].End)
}
