package tempo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3API is the part of the S3 client the store uses.
//
// It is an interface rather than *s3.Client so that the store can be tested
// without a bucket, and so the AWS types stay at this one edge of the package.
// *s3.Client satisfies it as it stands.
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// The real client has to satisfy the interface, or the claim above is just a
// comment and callers find out at their own call site.
var _ S3API = (*s3.Client)(nil)

// S3Store caches days in an S3 bucket, one object per day at
// Prefix/YYYY/MM/DD.json.
//
// It needs no write coordination: a day is written with the value RTE
// published for it, so two processes racing on the same key write the same
// bytes, and a single PutObject is atomic.
type S3Store struct {
	// Client reaches S3. Required; an *s3.Client will do.
	Client S3API
	// Bucket holds the tree. Required.
	Bucket string
	// Prefix places the tree inside the bucket. Optional; it is added to and
	// stripped from keys so the rest of the package never sees it.
	Prefix string
}

// Get returns the object at key, or ErrNotCached if the bucket has none.
func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s.object(key)),
	})
	if isMissing(err) {
		return nil, fmt.Errorf("tempo: %s: %w", key, ErrNotCached)
	}
	if err != nil {
		return nil, fmt.Errorf("tempo: get s3://%s/%s: %w", s.Bucket, s.object(key), err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("tempo: read s3://%s/%s: %w", s.Bucket, s.object(key), err)
	}
	return data, nil
}

// Put writes data at key.
func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(s.object(key)),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("tempo: put s3://%s/%s: %w", s.Bucket, s.object(key), err)
	}
	return nil
}

// List returns the keys under prefix, following every page.
//
// A month holds at most 31 objects, comfortably inside one page, but S3 is
// entitled to return fewer results than asked for at any time. Stopping at the
// first page would leave the cache believing it holds less than it does, and
// re-fetching days it already has on every window it plans.
func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	// Built by concatenation rather than through object(), because the trailing
	// slash is load-bearing — it is what keeps the listing to one month — and
	// path.Join would clean it away.
	full := s.root() + prefix

	keys := []string{}
	var token *string
	for {
		out, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.Bucket),
			Prefix:            aws.String(full),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("tempo: list s3://%s/%s: %w", s.Bucket, full, err)
		}
		for _, object := range out.Contents {
			if object.Key != nil {
				keys = append(keys, s.key(*object.Key))
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			return keys, nil
		}
		token = out.NextContinuationToken
	}
}

// root is Prefix as a bucket path, with exactly one trailing slash, or empty
// when the tree sits at the top of the bucket.
func (s *S3Store) root() string {
	if s.Prefix == "" {
		return ""
	}
	return strings.Trim(s.Prefix, "/") + "/"
}

// object turns a cache key into a bucket key.
func (s *S3Store) object(key string) string { return s.root() + key }

// key turns a bucket key back into a cache key, so the rest of the package
// never sees where in the bucket the tree happens to live.
func (s *S3Store) key(object string) string { return strings.TrimPrefix(object, s.root()) }

// isMissing reports whether err means the object simply is not there.
//
// S3 says so in more than one way depending on the request and on which
// implementation is answering, and reading any of them as a failure would turn
// every cold read into an outage.
func isMissing(err error) bool {
	if err == nil {
		return false
	}
	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}

	var responseErr interface{ HTTPStatusCode() int }
	return errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound
}
