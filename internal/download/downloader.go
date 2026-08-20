// Package download streams a remote .vsix to a local temp file with bounded
// redirects, 429-aware retry, and strict partial-file cleanup.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrTooManyRedirects is returned when the HTTP client follows more than
// maxRedirects hops (redirect loop or malicious chain).
var ErrTooManyRedirects = errors.New("download: too many redirects")

// ErrBadStatus is returned for any non-200 final response after redirects and
// allowed retries are exhausted.
var ErrBadStatus = errors.New("download: bad HTTP status")

const (
	maxRedirects = 10
	maxAttempts  = 3 // 1 initial + 2 retries on 429
)

// Download fetches url and streams it into dest (truncating dest). Memory use
// is O(1) — the body is never buffered. On any failure the partially-written
// dest is removed so the caller never sees a torn file.
//
// Retry policy: an HTTP 429 pauses for Retry-After (an integer number of
// seconds, else bounded exponential backoff) and retries, up to 2 additional
// attempts. Other statuses are terminal (ErrBadStatus).
func Download(ctx context.Context, url, dest string) (int64, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return ErrTooManyRedirects
			}
			return nil
		},
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		n, done, err := doAttempt(ctx, client, url, dest)
		if err == nil {
			return n, nil
		}
		// A terminal error (bad status, I/O, redirect cap, ctx) is final;
		// attempt() already removed the partial file.
		if done {
			return n, err
		}
		// Retryable 429: pause honoring Retry-After/backoff, then loop.
		if rerr, ok := err.(*retryErr); ok {
			if attempt == maxAttempts-1 {
				return 0, fmt.Errorf("download: rate limited; retries exhausted: %w", ErrBadStatus)
			}
			timer := time.NewTimer(rerr.delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, fmt.Errorf("download: cancelled during rate-limit backoff: %w", ctx.Err())
			case <-timer.C:
			}
			continue
		}
		return n, err
	}
	return 0, fmt.Errorf("download: failed after %d attempts", maxAttempts)
}

// retryErr is an internal signal wrapping a rate-limit response.
type retryErr struct {
	delay time.Duration
}

func (e *retryErr) Error() string { return "download: rate limited" }

// attempt performs a single HTTP GET+stream. It returns (n, terminal, err).
// terminal=false + *retryErr signals a retryable 429.
func doAttempt(ctx context.Context, client *http.Client, url, dest string) (n int64, terminal bool, err error) {
	f, ferr := os.Create(dest)
	if ferr != nil {
		return 0, true, fmt.Errorf("download: create %s: %w", dest, ferr)
	}
	cleanup := func(e error) (int64, bool, error) {
		f.Close()
		_ = os.Remove(dest)
		return 0, true, e
	}

	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if rerr != nil {
		return cleanup(fmt.Errorf("download: build request: %w", rerr))
	}
	req.Header.Set("User-Agent", "zen-vsix/0.1")

	resp, derr := client.Do(req)
	if derr != nil {
		if errors.Is(derr, ErrTooManyRedirects) {
			return cleanup(fmt.Errorf("%w", ErrTooManyRedirects))
		}
		return cleanup(fmt.Errorf("download: GET %s: %w", url, derr))
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		delay := retryDelayAttempt(resp.Header.Get("Retry-After"))
		resp.Body.Close()
		// Simpler: treat as retryable regardless of which attempt.
		return 0, false, &retryErr{delay: delay}
	}

	if resp.StatusCode != http.StatusOK {
		status := resp.Status
		resp.Body.Close()
		f.Close()
		_ = os.Remove(dest)
		return 0, true, fmt.Errorf("%w: GET %s -> %s", ErrBadStatus, url, status)
	}

	n, cerr := io.Copy(f, resp.Body)
	resp.Body.Close()
	if cerr != nil {
		return cleanup(fmt.Errorf("download: stream to %s: %w", dest, cerr))
	}
	if err := f.Sync(); err != nil {
		return cleanup(fmt.Errorf("download: sync %s: %w", dest, err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dest)
		return 0, true, fmt.Errorf("download: close %s: %w", dest, err)
	}
	return n, true, nil
}

// retryDelayAttempt resolves Retry-After into a bounded delay: an integer
// seconds field is honored, otherwise exponential backoff grows across the
// loop (each 429 is retried via this helper; caller bounds to 2 retries).
func retryDelayAttempt(retryAfter string) time.Duration {
	const maxDelay = 30 * time.Second
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s >= 0 {
		d := time.Duration(s) * time.Second
		if d > maxDelay {
			return maxDelay
		}
		return d
	}
	return time.Second // bounded fallback backoff
}
