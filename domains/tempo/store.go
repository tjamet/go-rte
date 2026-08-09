package tempo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNotCached is returned by [Store.Get] when the key holds nothing.
var ErrNotCached = errors.New("tempo: not cached")

// Store persists one JSON object per Tempo day, keyed YYYY/MM/DD.json.
//
// List exists for the sake of remote stores. Planning a fetch window needs to
// know which of 360 days are already held, and asking that one key at a time
// would cost more round trips than the API call it saves; listing the handful
// of month prefixes a window spans answers it in at most thirteen.
type Store interface {
	// Get returns the object at key, or ErrNotCached if there is none.
	Get(ctx context.Context, key string) ([]byte, error)
	// Put writes data at key, replacing whatever was there.
	Put(ctx context.Context, key string, data []byte) error
	// List returns the keys under prefix. An unused prefix is empty, not an error.
	List(ctx context.Context, prefix string) ([]string, error)
}

// FloorKey holds the earliest day RTE has data for, learned from responses
// rather than configured. Without it, every request for pre-history would walk
// off the bottom of the archive and call the API to be told nothing again.
const FloorKey = "floor.json"

// floor is the decoded FloorKey object.
type floor struct {
	Earliest Date `json:"earliest"`
}

// entry is one cached day.
//
// There is deliberately no fallback field. A degraded-mode value is provisional
// and may still change, so it is served straight from the fetch and never
// written; everything read back from the store is therefore final, and needs no
// expiry check.
type entry struct {
	Start     time.Time `json:"start,omitzero"`
	End       time.Time `json:"end,omitzero"`
	Colour    Colour    `json:"colour,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Absent records that RTE has no colour for this day, so that a hole in the
	// archive is asked about once rather than on every request.
	Absent bool `json:"absent,omitempty"`
}

func entryOf(d Day) entry {
	return entry{Start: d.Start, End: d.End, Colour: d.Colour, UpdatedAt: d.UpdatedAt}
}

func absentEntry() entry { return entry{Absent: true} }

func (e entry) day() Day {
	return Day{Start: e.Start, End: e.End, Colour: e.Colour, UpdatedAt: e.UpdatedAt}
}

func encodeEntry(e entry) ([]byte, error) {
	encoded, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("tempo: encode entry: %w", err)
	}
	return encoded, nil
}

func decodeEntry(data []byte) (entry, error) {
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return entry{}, fmt.Errorf("tempo: decode entry: %w", err)
	}
	return e, nil
}
