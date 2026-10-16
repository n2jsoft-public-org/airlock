package orgconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// Refresh fetches src into the cache at cachePath. It reports whether the body changed from the
// cached one of the same source. valid rejects a body the caller cannot apply: the cache then
// keeps its body, with the rejection in Rejected until a later read succeeds, and the error is
// returned. File sources are read directly and never cached.
func Refresh(ctx context.Context, f *Fetcher, src Source, cachePath string, now time.Time, valid func([]byte) error) (changed bool, err error) { //nolint:gocritic // Source by value is the public contract.
	if src.Kind == KindFile {
		return false, errors.New("file sources are not cached")
	}
	etag := ""
	if first, ok := LoadCache(cachePath).For(src.Raw); ok {
		etag = first.ETag
	}
	got, err := f.Fetch(ctx, src, etag)
	if err != nil {
		return false, err
	}
	// Built from the cache as it is after the fetch: a diff the CLI showed and cleared meanwhile
	// stays cleared.
	old, cached := LoadCache(cachePath).For(src.Raw)
	if got.NotModified {
		if !cached {
			return false, fmt.Errorf("%w: not modified without a cached copy", ErrUnreachable)
		}
		old.FetchedAt, old.Rejected = now, ""
		return false, SaveCache(cachePath, old)
	}
	if err := valid(got.Body); err != nil {
		if cached {
			old.Rejected = err.Error()
			return false, errors.Join(err, SaveCache(cachePath, old))
		}
		return false, err
	}
	next := Cache{Source: src.Raw, FetchedAt: now, ETag: got.ETag, Body: got.Body}
	if cached {
		next.Previous = old.Previous
		if !bytes.Equal(old.Body, got.Body) {
			changed = true
			if next.Previous == nil {
				next.Previous = old.Body
			}
		}
	} else {
		changed = true
	}
	return changed, SaveCache(cachePath, next)
}
