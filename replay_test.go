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
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/liquidgecka/testlib"
)

func TestDefaultMatcher(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	left := &RequestResponse{}
	right := &RequestResponse{
		Request: &http.Request{
			Method: http.MethodGet,
			URL: &url.URL{
				Scheme:   "http",
				Opaque:   "opaque",
				User:     url.UserPassword("user", "password"),
				Host:     "host",
				Path:     "path",
				RawPath:  "rawpath",
				RawQuery: "raw",
				Fragment: "fragment",
			},
			Header: http.Header{
				"header1": []string{"value1", "value2"},
			},
			Trailer: http.Header{
				"header2": []string{"value3", "value4"},
			},
		},
		RequestBody: []byte("body"),
	}

	// Nil values.
	T.Equal(DefaultMatcher(nil, nil), false)
	T.Equal(DefaultMatcher(left, right), false)

	// Nil URL.
	left.Request = &http.Request{}
	T.Equal(DefaultMatcher(left, right), false)

	// Different HTTP method.
	left.Request = &http.Request{
		Method: http.MethodPost,
		URL:    &url.URL{Scheme: "http"},
	}
	T.Equal(DefaultMatcher(left, right), false)

	// Empty method normalizes to GET.
	left.Request.Method = ""

	// Different Scheme.
	left.Request.URL.Scheme = "https"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.Scheme = right.Request.URL.Scheme

	// Different Opaque.
	left.Request.URL.Opaque = "NOT_OPAQUE"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.Opaque = right.Request.URL.Opaque

	// Different Host.
	left.Request.URL.Host = "NOT_HOST"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.Host = right.Request.URL.Host

	// Different Path.
	left.Request.URL.Path = "NOT_PATH"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.Path = right.Request.URL.Path

	// Different RawPath.
	left.Request.URL.RawPath = "NOT_RAW_PATH"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.RawPath = right.Request.URL.RawPath

	// Different RawQuery.
	left.Request.URL.RawQuery = "NOT_RAW_QUERY"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.RawQuery = right.Request.URL.RawQuery

	// Different Fragment.
	left.Request.URL.Fragment = "NOT_FRAGMENT"
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.URL.Fragment = right.Request.URL.Fragment

	// Left URL.User == nil.
	T.Equal(DefaultMatcher(left, right), false)

	// Right URL.User == nil.
	left.Request.URL.User = right.Request.URL.User
	right.Request.URL.User = nil
	T.Equal(DefaultMatcher(left, right), false)

	// Different URL.User.
	right.Request.URL.User = url.UserPassword("not_user", "not_password")
	T.Equal(DefaultMatcher(left, right), false)
	right.Request.URL.User = left.Request.URL.User

	// Different RequestBody.
	left.RequestBody = []byte("NOT_THE_SAME")
	T.Equal(DefaultMatcher(left, right), false)
	left.RequestBody = right.RequestBody

	// Different Headers.
	left.Request.Header = http.Header{
		"header1": []string{"value1", "value2_XXX"},
	}
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.Header = right.Request.Header

	// Different Trailers.
	left.Request.Trailer = http.Header{
		"header2": []string{"value1", "value2_XXX"},
	}
	T.Equal(DefaultMatcher(left, right), false)
	left.Request.Trailer = right.Request.Trailer

	// Successful match consumes right entry.
	T.Equal(DefaultMatcher(left, right), true)

	// Second attempt against already-consumed entry fails.
	T.Equal(DefaultMatcher(left, right), false)

	// Empty header map matches nil header map.
	leftEmpty := &RequestResponse{
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Scheme: "http", Host: "example.com"},
			Header: http.Header{},
		},
	}
	rightNil := &RequestResponse{
		Request: &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Scheme: "http", Host: "example.com"},
		},
	}
	T.Equal(DefaultMatcher(leftEmpty, rightNil), true)
}

func TestBadVersion(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	archivePath := T.WriteTempFile("\x00\x00\x00\x00")
	_, err := New(
		WithMode(ModeReplay),
		WithFile(archivePath),
	)
	T.ExpectError(err)
	T.Equal(errors.Is(err, ErrUnsupportedVersion), true)

	// Truncated header returns an error too.
	shortPath := filepath.Join(T.TempDir(), "short.dvr")
	shortFile := T.WriteTempFile("\x00")
	_ = shortPath
	_, err = New(
		WithMode(ModeReplay),
		WithFile(shortFile),
	)
	T.ExpectError(err)
}

func TestNoMatchError(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	err := &NoMatchError{
		Method: http.MethodPost,
		URL:    "http://example.com/test",
		Header: http.Header{
			"Z-Header": []string{"z"},
			"A-Header": []string{"a1", "a2"},
		},
		Trailer: http.Header{
			"X-Trailer": []string{"t"},
		},
		Body: []byte("short body"),
	}
	T.Equal(errors.Is(err, ErrNoMatch), true)
	T.ExpectErrorMessage(err, "Method: POST")
	T.ExpectErrorMessage(err, "URL: http://example.com/test")
	T.ExpectErrorMessage(err, "A-Header: a1, a2\n    Z-Header: z")
	T.ExpectErrorMessage(err, "X-Trailer: t")
	T.ExpectErrorMessage(err, "Body:\nshort body")

	longErr := &NoMatchError{
		Method: http.MethodPost,
		URL:    "http://example.com/long",
		Body:   bytes.Repeat([]byte("A"), 600),
	}
	T.ExpectErrorMessage(longErr, "... (content truncated by dvr)")
}
