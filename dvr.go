// Copyright 2014 Orchestrate, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dvr

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// DefaultArchiveFile is the default path used when recording or replaying
// HTTP traffic if no explicit file path is specified.
const DefaultArchiveFile = "testdata/archive.dvr"

// Mode determines how a [Recorder] handles outgoing HTTP requests.
type Mode int

const (
	// ModePassthrough forwards all requests directly to the underlying transport
	// without recording or replaying them.
	ModePassthrough Mode = iota

	// ModeRecord forwards requests to the underlying transport and persists each
	// request/response exchange to the configured archive file.
	ModeRecord

	// ModeReplay matches incoming requests against a previously recorded archive
	// file and returns the recorded responses without network access.
	ModeReplay
)

// String returns a human-readable name for the mode.
func (m Mode) String() string {
	switch m {
	case ModePassthrough:
		return "passthrough"
	case ModeRecord:
		return "record"
	case ModeReplay:
		return "replay"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

var (
	// ErrNoMatch indicates that no recorded request in the archive matched an
	// incoming request during replay.
	ErrNoMatch = errors.New("dvr: no matching recorded request")

	// ErrUnsupportedVersion indicates that an archive file has an unknown or
	// unsupported format version.
	ErrUnsupportedVersion = errors.New("dvr: unsupported archive version")

	// ErrClosed indicates that an operation was attempted on a closed [Recorder].
	ErrClosed = errors.New("dvr: recorder is closed")
)

const maxNoMatchBodyPreview = 512

// NoMatchError is returned by [Recorder.RoundTrip] during [ModeReplay] when an
// incoming request does not match any unconsumed entry in the archive.
type NoMatchError struct {
	Method  string
	URL     string
	Header  http.Header
	Trailer http.Header
	Body    []byte
}

// Error returns a formatted diagnostic describing the unmatched request.
func (e *NoMatchError) Error() string {
	var b strings.Builder
	b.WriteString("dvr: no matching recorded request\n\nDetails of the unmatched request:\n")
	fmt.Fprintf(&b, "Method: %s\n", e.Method)
	fmt.Fprintf(&b, "URL: %s\n", e.URL)

	if len(e.Header) > 0 {
		b.WriteString("Headers:\n")
		for _, key := range slices.Sorted(maps.Keys(e.Header)) {
			fmt.Fprintf(&b, "    %s: %s\n", key, strings.Join(e.Header[key], ", "))
		}
	}

	if len(e.Trailer) > 0 {
		b.WriteString("Trailers:\n")
		for _, key := range slices.Sorted(maps.Keys(e.Trailer)) {
			fmt.Fprintf(&b, "    %s: %s\n", key, strings.Join(e.Trailer[key], ", "))
		}
	}

	if len(e.Body) > 0 {
		b.WriteString("Body:\n")
		body := e.Body
		truncated := false
		if len(body) > maxNoMatchBodyPreview {
			body = body[:maxNoMatchBodyPreview]
			truncated = true
		}
		b.Write(body)
		if truncated {
			b.WriteString("... (content truncated by dvr)")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// Unwrap returns [ErrNoMatch] so callers can match with [errors.Is].
func (e *NoMatchError) Unwrap() error {
	return ErrNoMatch
}

// RequestResponse stores a recorded HTTP request/response exchange for use by
// [MatchFunc] and [ObfuscateFunc].
type RequestResponse struct {
	// Request is the HTTP request passed to RoundTrip. Its Body field is nil;
	// the captured bytes and any read error are stored in RequestBody and
	// RequestBodyError.
	Request          *http.Request
	RequestBody      []byte
	RequestBodyError error

	// Response is the HTTP response returned by the server. Its Body field is
	// nil; the captured bytes and any read error are stored in ResponseBody and
	// ResponseBodyError.
	Response          *http.Response
	ResponseBody      []byte
	ResponseBodyError error

	// Error is the error returned by the underlying RoundTrip call.
	Error error

	// UserData holds arbitrary state attached by a [MatchFunc] (for example,
	// marking an entry as already consumed).
	UserData any
}

// MatchFunc reports whether an incoming request (left) matches a recorded
// entry from the archive (right). Implementations should not mutate either
// argument except for right.UserData. During matching, left's Response* fields
// are unset.
type MatchFunc func(left, right *RequestResponse) bool

// ObfuscateFunc mutates a deep copy of a [RequestResponse] before it is written
// to an archive file during [ModeRecord], leaving the live HTTP request and
// response returned to the caller untouched.
type ObfuscateFunc func(*RequestResponse)

type config struct {
	mode       Mode
	file       string
	transport  http.RoundTripper
	matcher    MatchFunc
	obfuscator ObfuscateFunc
}

// Option configures a [Recorder] created with [New] or [NewForTest].
type Option func(*config)

// WithMode sets the operating [Mode] of the recorder.
func WithMode(mode Mode) Option {
	return func(c *config) {
		c.mode = mode
	}
}

// WithFile sets the archive file path used for [ModeRecord] and [ModeReplay].
func WithFile(path string) Option {
	return func(c *config) {
		c.file = path
	}
}

// WithTransport sets the underlying [http.RoundTripper] used in
// [ModePassthrough] and [ModeRecord]. Defaults to [http.DefaultTransport].
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) {
		c.transport = rt
	}
}

// WithMatcher sets the [MatchFunc] used to match incoming requests against
// recorded entries during [ModeReplay]. Defaults to [DefaultMatcher].
func WithMatcher(fn MatchFunc) Option {
	return func(c *config) {
		c.matcher = fn
	}
}

// WithObfuscator sets an [ObfuscateFunc] called on a copy of each exchange
// before it is saved during [ModeRecord].
func WithObfuscator(fn ObfuscateFunc) Option {
	return func(c *config) {
		c.obfuscator = fn
	}
}

// Recorder intercepts HTTP requests via [http.RoundTripper] to pass through,
// record, or replay HTTP traffic.
type Recorder struct {
	mode       Mode
	filePath   string
	transport  http.RoundTripper
	matcher    MatchFunc
	obfuscator ObfuscateFunc

	mu          sync.Mutex
	closed      bool
	file        *os.File
	gzipWriter  *gzip.Writer
	tarWriter   *tar.Writer
	writerCount int
	entries     []*RequestResponse
}

var (
	_ http.RoundTripper = (*Recorder)(nil)
	_ io.Closer         = (*Recorder)(nil)
)

// New creates and initializes a [Recorder] with the provided options.
//
// In [ModeRecord], New opens or creates the archive file immediately; callers
// must invoke [Recorder.Close] to flush and finalize the archive.
// In [ModeReplay], New loads and decodes the archive file into memory.
func New(opts ...Option) (*Recorder, error) {
	cfg := config{
		mode:      ModePassthrough,
		file:      DefaultArchiveFile,
		transport: http.DefaultTransport,
		matcher:   DefaultMatcher,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	switch cfg.mode {
	case ModePassthrough, ModeRecord, ModeReplay:
	default:
		return nil, fmt.Errorf("dvr: invalid mode %d", cfg.mode)
	}

	if cfg.matcher == nil {
		cfg.matcher = DefaultMatcher
	}

	if cfg.mode != ModeReplay && cfg.transport == nil {
		cfg.transport = http.DefaultTransport
		if cfg.transport == nil {
			return nil, errors.New("dvr: transport must not be nil")
		}
	}

	r := &Recorder{
		mode:       cfg.mode,
		filePath:   cfg.file,
		transport:  cfg.transport,
		matcher:    cfg.matcher,
		obfuscator: cfg.obfuscator,
	}

	switch r.mode {
	case ModeRecord:
		if err := r.openForRecord(); err != nil {
			return nil, err
		}
	case ModeReplay:
		if err := r.loadForReplay(); err != nil {
			return nil, err
		}
	}

	return r, nil
}

// NewForTest creates a [Recorder] for use in a test and registers [Recorder.Close]
// with [testing.TB.Cleanup]. Any initialization error fails the test immediately.
func NewForTest(tb testing.TB, opts ...Option) *Recorder {
	tb.Helper()
	r, err := New(opts...)
	if err != nil {
		tb.Fatalf("dvr: failed to create recorder: %v", err)
	}
	tb.Cleanup(func() {
		if err := r.Close(); err != nil {
			tb.Errorf("dvr: failed to close recorder: %v", err)
		}
	})
	return r
}

// Mode returns the [Mode] configured for this recorder.
func (r *Recorder) Mode() Mode {
	return r.mode
}

// Client returns a new [http.Client] configured to send requests through this
// recorder.
func (r *Recorder) Client() *http.Client {
	return &http.Client{Transport: r}
}

// RoundTrip implements [http.RoundTripper] by passing through, recording, or
// replaying the request according to the recorder's configured [Mode].
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("dvr: request must not be nil")
	}

	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}

	switch r.mode {
	case ModeRecord:
		return r.record(req)
	case ModeReplay:
		return r.replay(req)
	default:
		return r.transport.RoundTrip(req)
	}
}

// Close flushes and closes any underlying archive writers and files. Close is
// safe for concurrent use and is idempotent.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}
	r.closed = true

	var errs []error
	if r.tarWriter != nil {
		if err := r.tarWriter.Close(); err != nil {
			errs = append(errs, err)
		}
		r.tarWriter = nil
	}
	if r.gzipWriter != nil {
		if err := r.gzipWriter.Close(); err != nil {
			errs = append(errs, err)
		}
		r.gzipWriter = nil
	}
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			errs = append(errs, err)
		}
		r.file = nil
	}

	return errors.Join(errs...)
}
