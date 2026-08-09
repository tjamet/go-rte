package tempo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeS3 implements the narrow slice of S3 the store uses, holding objects in a
// map and paginating a listing as the real service does.
type fakeS3 struct {
	objects map[string][]byte
	// pageSize splits a listing over several responses when set.
	pageSize int
	// getErr is returned by GetObject instead of an object when set.
	getErr error

	lastPut *s3.PutObjectInput
	lists   int
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	data, ok := f.objects[*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.lastPut = in
	f.objects[*in.Key] = data
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.lists++

	matching := []string{}
	for key := range f.objects {
		if strings.HasPrefix(key, *in.Prefix) {
			matching = append(matching, key)
		}
	}
	// A map has no order; the fake sorts so that pagination is reproducible.
	sortStrings(matching)

	from := 0
	if in.ContinuationToken != nil {
		for i, key := range matching {
			if key == *in.ContinuationToken {
				from = i
				break
			}
		}
	}
	to := len(matching)
	truncated := false
	if f.pageSize > 0 && from+f.pageSize < to {
		to = from + f.pageSize
		truncated = true
	}

	out := &s3.ListObjectsV2Output{IsTruncated: &truncated}
	for _, key := range matching[from:to] {
		out.Contents = append(out.Contents, types.Object{Key: &key})
	}
	if truncated {
		next := matching[to]
		out.NextContinuationToken = &next
	}
	return out, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func TestS3StoreReadsBackWhatItWrote(t *testing.T) {
	api := newFakeS3()
	store := &S3Store{Client: api, Bucket: "tempo"}

	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{"colour":"BLUE"}`)))

	got, err := store.Get(context.Background(), "2026/08/10.json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"colour":"BLUE"}`, string(got))
	require.NotNil(t, api.lastPut)
	assert.Equal(t, "tempo", *api.lastPut.Bucket)
	assert.Equal(t, "application/json", *api.lastPut.ContentType)
}

func TestS3StoreReportsAMissingKeyAsNotCached(t *testing.T) {
	store := &S3Store{Client: newFakeS3(), Bucket: "tempo"}

	_, err := store.Get(context.Background(), "2026/08/10.json")

	assert.ErrorIs(t, err, ErrNotCached)
}

// A bucket that answers 404 without a typed error still means "not cached", and
// mistaking it for a failure would turn every cold read into an outage.
func TestS3StoreTreatsAPlainNotFoundAsNotCached(t *testing.T) {
	api := newFakeS3()
	api.getErr = &types.NotFound{}
	store := &S3Store{Client: api, Bucket: "tempo"}

	_, err := store.Get(context.Background(), "2026/08/10.json")

	assert.ErrorIs(t, err, ErrNotCached)
}

func TestS3StoreReportsOtherFailures(t *testing.T) {
	api := newFakeS3()
	api.getErr = errors.New("the bucket is on fire")
	store := &S3Store{Client: api, Bucket: "tempo"}

	_, err := store.Get(context.Background(), "2026/08/10.json")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotCached, "an outage must not be read as an empty cache")
}

func TestS3StoreListsAMonth(t *testing.T) {
	api := newFakeS3()
	store := &S3Store{Client: api, Bucket: "tempo"}
	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{}`)))
	require.NoError(t, store.Put(context.Background(), "2026/08/11.json", []byte(`{}`)))
	require.NoError(t, store.Put(context.Background(), "2026/09/01.json", []byte(`{}`)))

	got, err := store.List(context.Background(), "2026/08/")

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"2026/08/10.json", "2026/08/11.json"}, got)
}

// A month holds up to 31 objects, well inside one page, but a bucket may answer
// with fewer than asked for at any time; a listing that stopped at the first
// page would silently look like a partly empty cache and refetch what is there.
func TestS3StoreFollowsListingPages(t *testing.T) {
	api := newFakeS3()
	api.pageSize = 2
	store := &S3Store{Client: api, Bucket: "tempo"}
	for _, key := range []string{"2026/08/10.json", "2026/08/11.json", "2026/08/12.json", "2026/08/13.json", "2026/08/14.json"} {
		require.NoError(t, store.Put(context.Background(), key, []byte(`{}`)))
	}

	got, err := store.List(context.Background(), "2026/08/")

	require.NoError(t, err)
	assert.Len(t, got, 5)
	assert.Greater(t, api.lists, 1, "the fake paginated, so the store had to follow")
}

func TestS3StorePlacesTheTreeUnderItsPrefix(t *testing.T) {
	api := newFakeS3()
	store := &S3Store{Client: api, Bucket: "tempo", Prefix: "cache/tempo"}

	require.NoError(t, store.Put(context.Background(), "2026/08/10.json", []byte(`{}`)))

	_, held := api.objects["cache/tempo/2026/08/10.json"]
	assert.True(t, held, "the prefix belongs in the bucket, not in the keys the cache uses")

	got, err := store.List(context.Background(), "2026/08/")
	require.NoError(t, err)
	assert.Equal(t, []string{"2026/08/10.json"}, got,
		"listing returns cache keys, so the prefix is stripped back off")
}
