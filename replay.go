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
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
)

// DefaultMatcher matches an incoming request (left) against an unconsumed
// recorded entry (right) by comparing HTTP method, URL components, request
// body bytes, headers, and trailers. Once matched, right.UserData is set to
// prevent matching the same recorded entry twice.
func DefaultMatcher(left, right *RequestResponse) bool {
	if left == nil || right == nil || right.UserData != nil {
		return false
	}
	if left.Request == nil || right.Request == nil {
		return false
	}

	lreq := left.Request
	rreq := right.Request

	if effectiveMethod(lreq.Method) != effectiveMethod(rreq.Method) {
		return false
	}

	if lreq.URL == nil || rreq.URL == nil {
		return false
	}
	if lreq.URL.Scheme != rreq.URL.Scheme ||
		lreq.URL.Opaque != rreq.URL.Opaque ||
		lreq.URL.Host != rreq.URL.Host ||
		lreq.URL.Path != rreq.URL.Path ||
		lreq.URL.RawPath != rreq.URL.RawPath ||
		lreq.URL.RawQuery != rreq.URL.RawQuery ||
		lreq.URL.Fragment != rreq.URL.Fragment {
		return false
	}

	if !userInfoEqual(lreq.URL.User, rreq.URL.User) {
		return false
	}

	if !bytes.Equal(left.RequestBody, right.RequestBody) {
		return false
	}

	if !headersEqual(lreq.Header, rreq.Header) {
		return false
	}
	if !headersEqual(lreq.Trailer, rreq.Trailer) {
		return false
	}

	right.UserData = struct{}{}
	return true
}

func effectiveMethod(method string) string {
	if method == "" {
		return http.MethodGet
	}
	return method
}

func userInfoEqual(a, b *url.Userinfo) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.String() == b.String()
}

func headersEqual(a, b http.Header) bool {
	return maps.EqualFunc(a, b, slices.Equal[[]string])
}

func (r *Recorder) loadForReplay() error {
	f, err := os.Open(r.filePath)
	if err != nil {
		return fmt.Errorf("dvr: open archive for reading: %w", err)
	}
	defer func() { _ = f.Close() }()

	var version uint32
	if err := binary.Read(f, binary.BigEndian, &version); err != nil {
		return fmt.Errorf("dvr: read archive version: %w", err)
	}
	if version != archiveVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("dvr: open gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var entries []*RequestResponse
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("dvr: read archive entry: %w", err)
		}

		var q gobQuery
		if err := gob.NewDecoder(tr).Decode(&q); err != nil {
			return fmt.Errorf("dvr: decode archive entry: %w", err)
		}

		rr, err := q.toRequestResponse()
		if err != nil {
			return err
		}
		entries = append(entries, rr)
	}

	r.entries = entries
	return nil
}

func (r *Recorder) replay(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}

	var reqBuf bytes.Buffer
	var reqBodyErr error
	if req.Body != nil {
		_, reqBodyErr = io.Copy(&reqBuf, req.Body)
		_ = req.Body.Close()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, ErrClosed
	}

	rrSource := &RequestResponse{
		Request:          req,
		RequestBody:      reqBuf.Bytes(),
		RequestBodyError: reqBodyErr,
	}

	var rrMatch *RequestResponse
	for _, rr := range r.entries {
		if r.matcher(rrSource, rr) {
			rrMatch = rr
			break
		}
	}

	if rrMatch == nil {
		var rawURL string
		if req.URL != nil {
			rawURL = req.URL.String()
		}
		return nil, &NoMatchError{
			Method:  effectiveMethod(req.Method),
			URL:     rawURL,
			Header:  req.Header.Clone(),
			Trailer: req.Trailer.Clone(),
			Body:    bytes.Clone(reqBuf.Bytes()),
		}
	}

	if rrMatch.Response == nil {
		return nil, rrMatch.Error
	}

	resp := new(http.Response)
	*resp = *rrMatch.Response
	if rrMatch.Response.Header != nil {
		resp.Header = rrMatch.Response.Header.Clone()
	} else {
		resp.Header = make(http.Header)
	}
	if rrMatch.Response.Trailer != nil {
		resp.Trailer = rrMatch.Response.Trailer.Clone()
	}
	resp.Request = req
	resp.Body = &bodyReader{
		data: rrMatch.ResponseBody,
		err:  rrMatch.ResponseBodyError,
	}

	return resp, rrMatch.Error
}

type bodyReader struct {
	offset int
	data   []byte
	err    error
}

func (b *bodyReader) Read(p []byte) (int, error) {
	if b.offset >= len(b.data) {
		if b.err == nil {
			return 0, io.EOF
		}
		return 0, b.err
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	return n, nil
}

func (b *bodyReader) WriteTo(w io.Writer) (int64, error) {
	var total int64
	if b.offset < len(b.data) {
		n, err := w.Write(b.data[b.offset:])
		b.offset += n
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, b.err
}

func (b *bodyReader) Close() error {
	return nil
}
