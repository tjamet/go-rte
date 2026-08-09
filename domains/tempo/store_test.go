package tempo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDateMarshalsAsAnISOString(t *testing.T) {
	encoded, err := json.Marshal(struct {
		Earliest Date `json:"earliest"`
	}{Date{2014, time.September, 1}})

	require.NoError(t, err)
	assert.JSONEq(t, `{"earliest":"2014-09-01"}`, string(encoded))
}

func TestDateUnmarshalsFromAnISOString(t *testing.T) {
	var got struct {
		Earliest Date `json:"earliest"`
	}

	require.NoError(t, json.Unmarshal([]byte(`{"earliest":"2014-09-01"}`), &got))
	assert.Equal(t, Date{2014, time.September, 1}, got.Earliest)
}

func TestDateUnmarshalRejectsNonsense(t *testing.T) {
	var d Date

	assert.Error(t, json.Unmarshal([]byte(`"not-a-date"`), &d))
}

func TestPublishedEntryRoundTrips(t *testing.T) {
	paris := parisLocation(t)
	start, end := Date{2026, time.August, 10}.Range(paris)
	day := Day{
		Start:     start,
		End:       end,
		Colour:    Blue,
		UpdatedAt: time.Date(2026, time.August, 9, 10, 32, 0, 0, paris),
	}

	encoded, err := encodeEntry(entryOf(day))
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"start":"2026-08-10T00:00:00+02:00",
		"end":"2026-08-11T00:00:00+02:00",
		"colour":"BLUE",
		"updated_at":"2026-08-09T10:32:00+02:00"
	}`, string(encoded))

	decoded, err := decodeEntry(encoded)
	require.NoError(t, err)
	assert.False(t, decoded.Absent)

	// The offset survives the round trip even though the named zone does not,
	// which is exactly what Day promises: a Tempo day is anchored to Paris local
	// midnight, and the offset is what records where that midnight fell.
	got := decoded.day()
	assert.Equal(t, Blue, got.Colour)
	assert.Equal(t, day.Start.Format(time.RFC3339), got.Start.Format(time.RFC3339))
	assert.Equal(t, day.End.Format(time.RFC3339), got.End.Format(time.RFC3339))
	assert.Equal(t, day.UpdatedAt.Format(time.RFC3339), got.UpdatedAt.Format(time.RFC3339))
	assert.False(t, got.Fallback, "a cached day is always final")
}

// A cache entry never carries a fallback flag: degraded-mode values are not
// written at all, so anything read back is final.
func TestAbsentEntryIsMinimal(t *testing.T) {
	encoded, err := encodeEntry(absentEntry())
	require.NoError(t, err)
	assert.JSONEq(t, `{"absent":true}`, string(encoded))

	decoded, err := decodeEntry(encoded)
	require.NoError(t, err)
	assert.True(t, decoded.Absent)
}

func TestDecodeEntryRejectsGarbage(t *testing.T) {
	_, err := decodeEntry([]byte("not json"))

	assert.Error(t, err)
}

func TestFileStoreReadsBackWhatItWrote(t *testing.T) {
	store := &FileStore{Root: t.TempDir()}

	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{"colour":"BLUE"}`)))

	got, err := store.Get(context.Background(), "2026/08/10.json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"colour":"BLUE"}`, string(got))
}

func TestFileStoreReportsAMissingKeyAsNotCached(t *testing.T) {
	store := &FileStore{Root: t.TempDir()}

	_, err := store.Get(context.Background(), "2026/08/10.json")

	assert.ErrorIs(t, err, ErrNotCached)
}

func TestFileStoreOverwritesWithoutLeavingTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	store := &FileStore{Root: root}

	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{"colour":"BLUE"}`)))
	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{"colour":"RED"}`)))

	got, err := store.Get(context.Background(), "2026/08/10.json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"colour":"RED"}`, string(got))

	entries, err := os.ReadDir(filepath.Join(root, "2026", "08"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "the rename should leave only the final file")
	assert.Equal(t, "10.json", entries[0].Name())
}

func TestFileStoreListsTheDaysOfAMonth(t *testing.T) {
	store := &FileStore{Root: t.TempDir()}
	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{}`)))
	require.NoError(t, store.Put(context.Background(), "2026/08/11.json", []byte(`{}`)))
	require.NoError(t, store.Put(context.Background(), "2026/09/01.json", []byte(`{}`)))

	got, err := store.List(context.Background(), "2026/08/")

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"2026/08/10.json", "2026/08/11.json"}, got)
}

// A month nobody has cached yet is empty, not an error: the planner lists every
// month a window spans and most of them will be missing on a cold cache.
func TestFileStoreListsAnUncachedMonthAsEmpty(t *testing.T) {
	store := &FileStore{Root: t.TempDir()}

	got, err := store.List(context.Background(), "2026/08/")

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFileStoreRefusesToEscapeItsRoot(t *testing.T) {
	store := &FileStore{Root: t.TempDir()}

	err := store.Put(context.Background(), "../escaped.json", []byte(`{}`))

	assert.Error(t, err)
}

func parisLocation(t *testing.T) *time.Location {
	t.Helper()
	paris, err := time.LoadLocation(parisZone)
	require.NoError(t, err)
	return paris
}
