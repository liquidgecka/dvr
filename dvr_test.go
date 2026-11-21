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
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liquidgecka/testlib"
)

type httpHandler struct{}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Pin Date so direct and replayed responses remain deterministic.
	w.Header().Add("Date", "Mon, 2 Mar 2001 01:02:03 GMT")

	switch r.URL.Path {
	case "/201":
		w.WriteHeader(http.StatusCreated)
	case "/220":
		w.WriteHeader(220)
	case "/404":
		w.WriteHeader(http.StatusNotFound)
	case "/501":
		w.WriteHeader(http.StatusNotImplemented)
	case "/540":
		w.WriteHeader(540)
	case "/resp_header":
		w.Header().Add("X-Test-Header", "X Y Z")
		w.WriteHeader(http.StatusOK)
	case "/body":
		data, err := io.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}
		sum := md5.Sum(data)
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprintf(w, "%x\n", sum); err != nil {
			panic(err)
		}
	case "/error":
		_, _ = io.Copy(io.Discard, r.Body)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			panic("response writer does not implement http.Hijacker")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			panic(err)
		}
		if err := conn.Close(); err != nil {
			panic(err)
		}
	case "/request_body_error":
		_, _ = io.ReadAll(r.Body)
	default:
		panic(fmt.Errorf("unknown request to %s: %#v", r.URL.Path, r))
	}
}

type savedQuery struct {
	Request           *http.Request
	RequestBody       []byte
	RequestBodyError  error
	Response          *http.Response
	ResponseBody      []byte
	ResponseBodyError error
	Error             error
}

func runTests(
	ctx context.Context,
	T *testlib.T,
	rt http.RoundTripper,
	baseURL, username, password string,
) []*savedQuery {
	T.Helper()

	makeRequest := func(path, method, body string) *savedQuery {
		sq := &savedQuery{}

		req, err := http.NewRequestWithContext(
			ctx, method, baseURL+path, io.NopCloser(strings.NewReader(body)),
		)
		T.ExpectSuccess(err)
		req.Header.Add("X-Client-Header", "test")
		req.ContentLength = int64(len(body))
		if username != "" || password != "" {
			req.SetBasicAuth(username, password)
		}
		sq.Request = req
		sq.RequestBody = []byte(body)

		client := &http.Client{Transport: rt}
		resp, err := client.Do(req)
		sq.Response = resp
		sq.Error = err
		if resp != nil {
			var buf bytes.Buffer
			_, sq.ResponseBodyError = io.Copy(&buf, resp.Body)
			_ = resp.Body.Close()
			sq.ResponseBody = buf.Bytes()
			sq.Response.Body = nil
		}

		sq.Request.Body = nil
		sq.Request.GetBody = nil
		return sq
	}

	makeErrored := func(path string) *savedQuery {
		sq := &savedQuery{}

		bodyReader, bodyWriter := io.Pipe()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, bodyReader)
		T.ExpectSuccess(err)
		req.Header.Add("X-Client-Header", "test")

		sq.Request = req
		sq.RequestBody = []byte("testfailure")
		sq.RequestBodyError = errors.New("expected")
		go func() {
			_, _ = bodyWriter.Write(sq.RequestBody)
			_ = bodyWriter.CloseWithError(sq.RequestBodyError)
		}()

		client := &http.Client{Transport: rt}
		resp, err := client.Do(req)
		sq.Response = resp
		sq.Error = err
		if resp != nil {
			var buf bytes.Buffer
			_, sq.ResponseBodyError = io.Copy(&buf, resp.Body)
			_ = resp.Body.Close()
			sq.ResponseBody = buf.Bytes()
			sq.Response.Body = nil
		}

		sq.Request.Body = nil
		sq.Request.GetBody = nil
		return sq
	}

	results := make([]*savedQuery, 0, 40)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
		for _, path := range []string{"/201", "/220", "/404", "/501", "/540"} {
			results = append(results, makeRequest(path, method, "body1"))
			results = append(results, makeRequest(path, method, "body2"))
		}
	}

	for _, path := range []string{"/resp_header", "/body", "/error"} {
		results = append(results, makeRequest(path, http.MethodGet, "body1"))
		results = append(results, makeRequest(path, http.MethodGet, "body2"))
	}

	results = append(results, makeErrored("/request_body_error"))
	return results
}

func TestFullCycle(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	server := httptest.NewServer(&httpHandler{})
	defer server.Close()

	ctx := t.Context()
	transport := server.Client().Transport

	// Direct baseline without DVR.
	directResponses := runTests(ctx, T, transport, server.URL, "", "")

	// Passthrough mode.
	passthrough, err := New(
		WithMode(ModePassthrough),
		WithTransport(transport),
	)
	T.ExpectSuccess(err)
	T.Equal(passthrough.Mode(), ModePassthrough)
	passthroughResponses := runTests(ctx, T, passthrough, server.URL, "", "")
	T.ExpectSuccess(passthrough.Close())
	T.Equal(passthroughResponses, directResponses)

	// Record mode.
	archivePath := filepath.Join(T.TempDir(), "archive.dvr")
	recorder, err := New(
		WithMode(ModeRecord),
		WithFile(archivePath),
		WithTransport(transport),
	)
	T.ExpectSuccess(err)
	T.Equal(recorder.Mode(), ModeRecord)
	recordResponses := runTests(ctx, T, recorder, server.URL, "", "")
	T.ExpectSuccess(recorder.Close())
	T.Equal(recordResponses, directResponses)

	// Replay mode.
	replayer, err := New(
		WithMode(ModeReplay),
		WithFile(archivePath),
	)
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(replayer.Close())
	}()
	T.Equal(replayer.Mode(), ModeReplay)

	replayResponses := runTests(ctx, T, replayer, server.URL, "", "")
	T.Equal(replayResponses, directResponses)

	// Verify /error replayed io.EOF via errors.Is.
	var sawEOF bool
	for _, sq := range replayResponses {
		if sq.Request.URL.Path == "/error" {
			T.ExpectError(sq.Error)
			T.Equal(errors.Is(sq.Error, io.EOF), true)
			sawEOF = true
		}
	}
	T.Equal(sawEOF, true)

	// Unmatched request returns a *NoMatchError wrapping ErrNoMatch.
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		server.URL+"/wtf",
		io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("TEST"), 256))),
	)
	T.ExpectSuccess(err)
	req.Header = http.Header{"TestHeader": []string{"Value1"}}
	req.Trailer = http.Header{"TestTrailer": []string{"Value2"}}

	resp, err := replayer.Client().Do(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	T.ExpectError(err)
	T.Equal(errors.Is(err, ErrNoMatch), true)

	noMatchErr, ok := errors.AsType[*NoMatchError](err)
	T.Equal(ok, true)
	T.ExpectErrorMessage(noMatchErr, "URL: "+req.URL.String())
	T.ExpectErrorMessage(noMatchErr, "Method: GET")
	T.ExpectErrorMessage(noMatchErr, "Headers:")
	T.ExpectErrorMessage(noMatchErr, "TestHeader: Value1")
	T.ExpectErrorMessage(noMatchErr, "Trailers:")
	T.ExpectErrorMessage(noMatchErr, "TestTrailer: Value2")
	T.ExpectErrorMessage(noMatchErr, "Body:")
	T.ExpectErrorMessage(noMatchErr, "TESTTESTTESTTEST")
	T.ExpectErrorMessage(noMatchErr, "... (content truncated by dvr)")

	// Record with BasicAuthObfuscator using user1/pass1, replay using user2/pass2.
	obfArchive := filepath.Join(T.TempDir(), "obfuscated.dvr")
	obfRecorder, err := New(
		WithMode(ModeRecord),
		WithFile(obfArchive),
		WithTransport(transport),
		WithObfuscator(BasicAuthObfuscator("user2", "pass2")),
	)
	T.ExpectSuccess(err)
	runTests(ctx, T, obfRecorder, server.URL, "user1", "pass1")
	T.ExpectSuccess(obfRecorder.Close())

	obfReplayer, err := New(
		WithMode(ModeReplay),
		WithFile(obfArchive),
	)
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(obfReplayer.Close())
	}()
	obfReplayed := runTests(ctx, T, obfReplayer, server.URL, "user2", "pass2")
	T.NotEqual(len(obfReplayed), 0)
}

func TestTLSRecordAndReplay(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	server := httptest.NewTLSServer(&httpHandler{})
	defer server.Close()

	archivePath := filepath.Join(T.TempDir(), "tls.dvr")
	ctx := t.Context()

	rec, err := New(
		WithMode(ModeRecord),
		WithFile(archivePath),
		WithTransport(server.Client().Transport),
	)
	T.ExpectSuccess(err)

	req1, err := http.NewRequestWithContext(
		ctx, http.MethodPost, server.URL+"/body", strings.NewReader("tls-payload"),
	)
	T.ExpectSuccess(err)
	resp1, err := rec.Client().Do(req1)
	T.ExpectSuccess(err)
	body1, err := io.ReadAll(resp1.Body)
	T.ExpectSuccess(err)
	T.ExpectSuccess(resp1.Body.Close())
	T.ExpectSuccess(rec.Close())

	T.NotEqual(resp1.TLS, nil)
	T.NotEqual(len(resp1.TLS.PeerCertificates), 0)

	rep, err := New(
		WithMode(ModeReplay),
		WithFile(archivePath),
	)
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(rep.Close())
	}()

	req2, err := http.NewRequestWithContext(
		ctx, http.MethodPost, server.URL+"/body", strings.NewReader("tls-payload"),
	)
	T.ExpectSuccess(err)
	resp2, err := rep.Client().Do(req2)
	T.ExpectSuccess(err)
	body2, err := io.ReadAll(resp2.Body)
	T.ExpectSuccess(err)
	T.ExpectSuccess(resp2.Body.Close())

	T.Equal(resp2.StatusCode, http.StatusOK)
	T.Equal(body2, body1)
	T.NotEqual(resp2.TLS, nil)
	T.Equal(resp2.TLS.Version, resp1.TLS.Version)
	T.Equal(resp2.TLS.CipherSuite, resp1.TLS.CipherSuite)
	T.Equal(resp2.TLS.HandshakeComplete, resp1.TLS.HandshakeComplete)
	T.Equal(len(resp2.TLS.PeerCertificates), len(resp1.TLS.PeerCertificates))
	T.Equal(resp2.TLS.PeerCertificates[0].Raw, resp1.TLS.PeerCertificates[0].Raw)
}

func TestNewForTest(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	server := httptest.NewServer(&httpHandler{})
	defer server.Close()

	archivePath := filepath.Join(T.TempDir(), "cleanup.dvr")

	t.Run("record", func(t *testing.T) {
		rec := NewForTest(t,
			WithMode(ModeRecord),
			WithFile(archivePath),
			WithTransport(server.Client().Transport),
		)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/201", nil)
		T.ExpectSuccess(err)
		resp, err := rec.Client().Do(req)
		T.ExpectSuccess(err)
		T.ExpectSuccess(resp.Body.Close())
	})

	rep := NewForTest(t,
		WithMode(ModeReplay),
		WithFile(archivePath),
	)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/201", nil)
	T.ExpectSuccess(err)
	resp, err := rep.Client().Do(req)
	T.ExpectSuccess(err)
	T.ExpectSuccess(resp.Body.Close())
	T.Equal(resp.StatusCode, http.StatusCreated)
}

func TestCloseAndErrClosed(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	server := httptest.NewServer(&httpHandler{})
	defer server.Close()

	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{
			name: "passthrough",
			opts: []Option{WithMode(ModePassthrough), WithTransport(server.Client().Transport)},
		},
		{
			name: "record",
			opts: []Option{
				WithMode(ModeRecord),
				WithFile(filepath.Join(T.TempDir(), "closed.dvr")),
				WithTransport(server.Client().Transport),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := New(tc.opts...)
			T.ExpectSuccess(err)
			T.ExpectSuccess(rec.Close())
			T.ExpectSuccess(rec.Close()) // Idempotent.

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/201", nil)
			T.ExpectSuccess(err)
			resp, err := rec.RoundTrip(req)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			T.Equal(errors.Is(err, ErrClosed), true)
		})
	}

	// Verify ErrClosed in replay mode too.
	archivePath := filepath.Join(T.TempDir(), "replay-closed.dvr")
	rec, err := New(WithMode(ModeRecord), WithFile(archivePath), WithTransport(server.Client().Transport))
	T.ExpectSuccess(err)
	T.ExpectSuccess(rec.Close())

	rep, err := New(WithMode(ModeReplay), WithFile(archivePath))
	T.ExpectSuccess(err)
	T.ExpectSuccess(rep.Close())
	T.ExpectSuccess(rep.Close())

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/201", nil)
	T.ExpectSuccess(err)
	resp, err := rep.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	T.Equal(errors.Is(err, ErrClosed), true)

	// Nil request returns an error.
	pt, err := New(WithMode(ModePassthrough), WithTransport(server.Client().Transport))
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(pt.Close())
	}()
	resp, err = pt.RoundTrip(nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	T.ExpectError(err)
}

func TestConcurrentRecordAndReplay(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	server := httptest.NewServer(&httpHandler{})
	defer server.Close()

	archivePath := filepath.Join(T.TempDir(), "concurrent.dvr")
	rec, err := New(
		WithMode(ModeRecord),
		WithFile(archivePath),
		WithTransport(server.Client().Transport),
	)
	T.ExpectSuccess(err)

	const workers = 16
	ctx := t.Context()

	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			body := fmt.Sprintf("worker-%d", i)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/body", strings.NewReader(body))
			if err != nil {
				t.Errorf("request creation failed: %v", err)
				return
			}
			resp, err := rec.Client().Do(req)
			if err != nil {
				t.Errorf("record request failed: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		})
	}
	wg.Wait()
	T.ExpectSuccess(rec.Close())

	rep, err := New(
		WithMode(ModeReplay),
		WithFile(archivePath),
	)
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(rep.Close())
	}()

	for i := range workers {
		wg.Go(func() {
			body := fmt.Sprintf("worker-%d", i)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/body", strings.NewReader(body))
			if err != nil {
				t.Errorf("request creation failed: %v", err)
				return
			}
			resp, err := rep.Client().Do(req)
			if err != nil {
				t.Errorf("replay request failed: %v", err)
				return
			}
			got, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Errorf("read failed: %v", readErr)
				return
			}
			want := fmt.Sprintf("%x\n", md5.Sum([]byte(body)))
			if string(got) != want {
				t.Errorf("body mismatch: got %q, want %q", got, want)
			}
		})
	}
	wg.Wait()
}

func TestReplayContextCanceled(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	archivePath := filepath.Join(T.TempDir(), "empty.dvr")
	rec, err := New(WithMode(ModeRecord), WithFile(archivePath))
	T.ExpectSuccess(err)
	T.ExpectSuccess(rec.Close())

	rep, err := New(WithMode(ModeReplay), WithFile(archivePath))
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(rep.Close())
	}()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/test", nil)
	T.ExpectSuccess(err)
	resp, err := rep.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	T.Equal(errors.Is(err, context.Canceled), true)
}

func TestCustomMatcher(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	server := httptest.NewServer(&httpHandler{})
	defer server.Close()

	archivePath := filepath.Join(T.TempDir(), "custom-matcher.dvr")
	ctx := t.Context()

	rec, err := New(
		WithMode(ModeRecord),
		WithFile(archivePath),
		WithTransport(server.Client().Transport),
	)
	T.ExpectSuccess(err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/201?token=secret1", nil)
	T.ExpectSuccess(err)
	resp, err := rec.Client().Do(req)
	T.ExpectSuccess(err)
	T.ExpectSuccess(resp.Body.Close())
	T.ExpectSuccess(rec.Close())

	// Match purely by URL path, ignoring query parameters and allowing reuse.
	pathMatcher := func(left, right *RequestResponse) bool {
		return left.Request.URL.Path == right.Request.URL.Path
	}
	rep, err := New(
		WithMode(ModeReplay),
		WithFile(archivePath),
		WithMatcher(pathMatcher),
	)
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(rep.Close())
	}()

	for _, q := range []string{"secret2", "secret3"} {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/201?token="+q, nil)
		T.ExpectSuccess(err)
		got, err := rep.Client().Do(r)
		T.ExpectSuccess(err)
		T.ExpectSuccess(got.Body.Close())
		T.Equal(got.StatusCode, http.StatusCreated)
	}
}

func TestNewErrors(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	_, err := New(WithMode(Mode(99)))
	T.ExpectError(err)

	_, err = New(
		WithMode(ModeReplay),
		WithFile(filepath.Join(T.TempDir(), "does-not-exist.dvr")),
	)
	T.ExpectError(err)

	// Empty archive replays with ErrNoMatch.
	emptyPath := filepath.Join(T.TempDir(), "empty.dvr")
	rec, err := New(WithMode(ModeRecord), WithFile(emptyPath))
	T.ExpectSuccess(err)
	T.ExpectSuccess(rec.Close())

	rep, err := New(WithMode(ModeReplay), WithFile(emptyPath))
	T.ExpectSuccess(err)
	defer func() {
		T.ExpectSuccess(rep.Close())
	}()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", nil)
	T.ExpectSuccess(err)
	resp, err := rep.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	T.Equal(errors.Is(err, ErrNoMatch), true)
}

func TestBasicAuthObfuscator(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	obf := BasicAuthObfuscator("user1", "pass1")
	obf(nil)
	obf(&RequestResponse{})

	rr := &RequestResponse{
		Request: &http.Request{
			URL: &url.URL{
				User: url.UserPassword("user2", "pass2"),
			},
			Header: http.Header{
				"Authorization": []string{"test"},
			},
		},
	}
	obf(rr)

	T.Equal(rr.Request.URL.User.String(), "user1:pass1")
	T.Equal(rr.Request.Header.Get("Authorization"), "Basic dXNlcjE6cGFzczE=")

	obfNoPass := BasicAuthObfuscator("user1", "")
	obfNoPass(rr)
	T.Equal(rr.Request.URL.User.String(), "user1")
	T.Equal(rr.Request.Header.Get("Authorization"), "Basic dXNlcjE6")
}

func TestModeString(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	T.Equal(ModePassthrough.String(), "passthrough")
	T.Equal(ModeRecord.String(), "record")
	T.Equal(ModeReplay.String(), "replay")
	T.Equal(Mode(42).String(), "Mode(42)")
}
