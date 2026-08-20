package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownload302ChainOK(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hits.Add(1) {
		case 1:
			http.Redirect(w, r, "/two", http.StatusFound)
		case 2:
			http.Redirect(w, r, "/three", http.StatusMovedPermanently)
		default:
			_, _ = w.Write([]byte("payload"))
		}
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	n, err := Download(context.Background(), ts.URL+"/one", dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n != 7 {
		t.Fatalf("n = %d, want 7", n)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != "payload" {
		t.Fatalf("dest content: %q err=%v", b, err)
	}
}

func TestDownload404IsErrBadStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	_, err := Download(context.Background(), ts.URL, dest)
	if !errors.Is(err, ErrBadStatus) {
		t.Fatalf("err = %v, want ErrBadStatus", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial dest must be removed on bad status, stat err = %v", statErr)
	}
}

func TestDownloadTooManyRedirects(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	_, err := Download(context.Background(), ts.URL, dest)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
}

func TestDownload429RetryThenSuccess(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("finally"))
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	n, err := Download(context.Background(), ts.URL, dest)
	if err != nil {
		t.Fatalf("Download after 429s: %v", err)
	}
	if hits.Load() != 3 {
		t.Fatalf("expected 3 requests (2×429 + success), got %d", hits.Load())
	}
	if n != 7 {
		t.Fatalf("n = %d", n)
	}
}

func TestDownload429Exhausted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	_, err := Download(context.Background(), ts.URL, dest)
	if err == nil {
		t.Fatal("persistent 429 must error after retries")
	}
	if !errors.Is(err, ErrBadStatus) {
		t.Fatalf("err = %v, want wrapped ErrBadStatus", err)
	}
}

func TestDownloadMidStreamErrorRemovesPartial(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
		// Flush then abort the connection mid-body.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler) // server closes connection abruptly
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	_, err := Download(context.Background(), ts.URL, dest)
	if err == nil {
		t.Fatal("aborted body must error")
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial dest must be removed on mid-stream error, stat err = %v", statErr)
	}
}

func TestDownloadContextCancelRemovesPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel() // cancel immediately on first request
		w.Header().Set("Content-Length", "99999")
		_, _ = w.Write([]byte("x"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(50 * time.Millisecond) // yield; ctx cancel tears the conn down
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "out.vsix")
	_, err := Download(ctx, ts.URL, dest)
	if err == nil {
		t.Fatal("cancelled download must error")
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial dest must be removed on cancellation, stat err = %v", statErr)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if d := retryDelayAttempt("2"); d != 2_000_000_000 {
		t.Fatalf("Retry-After 2 → %v", d)
	}
	if d := retryDelayAttempt("bogus"); d <= 0 {
		t.Fatalf("bogus Retry-After must fall back to positive backoff, got %v", d)
	}
	if d := retryDelayAttempt("999999"); d > 30_000_000_000 {
		t.Fatalf("Retry-After must be capped, got %v", d)
	}
}
