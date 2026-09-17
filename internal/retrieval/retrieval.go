// Package retrieval provides the mechanisms collectors use to obtain source
// content. Collectors depend on the narrow interfaces here, so a source that
// moves from a plain download to a JavaScript-rendered page changes its
// collector, not the persistence or query layers.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Fetcher retrieves the body at a URL.
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// Browser runs a JavaScript-capable page session for a collector whose source
// only yields its data after scripts run or after interaction. The session
// ends, and the browser process with it, when fn returns.
type Browser interface {
	WithPage(ctx context.Context, fn func(Page) error) error
}

// Page is the deliberately small set of operations a collector may perform in
// a browser session. Selectors are CSS selectors. Every operation is bounded by
// the session's action timeout; element operations wait for the element to
// exist first.
type Page interface {
	// Navigate loads url in the page and waits for the load to finish.
	Navigate(url string) error
	// WaitVisible waits until an element matching selector is visible.
	WaitVisible(selector string) error
	// WaitFor polls a JavaScript expression until it is truthy.
	WaitFor(expression string) error
	// Click clicks the first element matching selector.
	Click(selector string) error
	// Evaluate runs a JavaScript expression and decodes its JSON-serialisable
	// result into out.
	Evaluate(expression string, out any) error
	// HTML returns the outer HTML of the first element matching selector.
	HTML(selector string) (string, error)
	// Text returns the visible text of the first element matching selector.
	Text(selector string) (string, error)
	// Attribute returns an attribute of the first element matching selector
	// and whether it is present.
	Attribute(selector, name string) (string, bool, error)
}

// DefaultTimeout bounds a single fetch.
const DefaultTimeout = 60 * time.Second

// DefaultMaxBytes bounds a single response.
const DefaultMaxBytes = 64 << 20

// ErrTruncated reports a response that ended before its declared length or
// exceeded the size bound. Either way it must not be treated as complete.
var ErrTruncated = errors.New("response incomplete")

// HTTP is a bounded, identified HTTP fetcher.
type HTTP struct {
	Client    *http.Client
	UserAgent string
	MaxBytes  int64
	Timeout   time.Duration
}

// NewHTTP builds a fetcher with sensible bounds.
func NewHTTP(userAgent string) *HTTP {
	return &HTTP{
		Client:    &http.Client{},
		UserAgent: userAgent,
		MaxBytes:  DefaultMaxBytes,
		Timeout:   DefaultTimeout,
	}
}

// Get performs a GET and returns the complete body, or an error. A short read
// against Content-Length, an oversized body, a non-200 status, or an empty body
// are all errors.
func (h *HTTP) Get(ctx context.Context, url string) ([]byte, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}
	if h.UserAgent != "" {
		req.Header.Set("User-Agent", h.UserAgent)
	}
	req.Header.Set("Accept", "text/markdown, application/json, text/csv, application/zip, */*")

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: unexpected status %s", url, resp.Status)
	}

	limit := h.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w: %w", url, ErrTruncated, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("fetching %s: %w: body exceeds %d bytes", url, ErrTruncated, limit)
	}
	if resp.ContentLength > 0 && int64(len(body)) != resp.ContentLength {
		return nil, fmt.Errorf("fetching %s: %w: read %d of %d declared bytes", url, ErrTruncated, len(body), resp.ContentLength)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("fetching %s: empty response", url)
	}
	return body, nil
}

// Static is a Fetcher serving fixed bodies by URL, for tests and offline use.
type Static struct {
	Bodies map[string][]byte
	Errors map[string]error
	Calls  map[string]int
}

// Get returns the configured body or error for url.
func (s *Static) Get(_ context.Context, url string) ([]byte, error) {
	if s.Calls == nil {
		s.Calls = map[string]int{}
	}
	s.Calls[url]++
	if err, ok := s.Errors[url]; ok && err != nil {
		return nil, err
	}
	body, ok := s.Bodies[url]
	if !ok {
		return nil, fmt.Errorf("fetching %s: no static body configured", url)
	}
	return body, nil
}
