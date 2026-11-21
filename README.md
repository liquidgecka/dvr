# DVR

[![CI](https://github.com/liquidgecka/dvr/actions/workflows/ci.yml/badge.svg)](https://github.com/liquidgecka/dvr/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/liquidgecka/dvr.svg)](https://pkg.go.dev/github.com/liquidgecka/dvr)

DVR is a Go library for capturing and replaying HTTP calls during tests.

Inspired by Python's [VCR.py](https://github.com/kevin1024/vcrpy), DVR provides
an explicit, concurrency-safe `http.RoundTripper` implementation (`dvr.Recorder`)
that supports passthrough, recording, and offline replay modes without global
state or `init()` side effects.

## Installation

```bash
go get github.com/liquidgecka/dvr
```

Requires Go 1.26 or later.

## Usage

Create a `*dvr.Recorder` in your test using `dvr.NewForTest` (which
automatically flushes and closes the archive via `t.Cleanup`) or `dvr.New`:

```go
package mypkg_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/liquidgecka/dvr"
)

func TestAPI(t *testing.T) {
	rec := dvr.NewForTest(t,
		dvr.WithMode(dvr.ModeReplay),
		dvr.WithFile("testdata/api.dvr"),
	)

	resp, err := rec.Client().Get("https://api.example.com/resource")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	_, _ = io.ReadAll(resp.Body)
}
```

### Command-Line Flags

If you want `go test` flags (`-dvr.record`, `-dvr.replay`, `-dvr.passthrough`,
and `-dvr.file`), register them explicitly in your test package:

```go
var dvrFlags = dvr.RegisterFlags(nil)

func TestWithFlags(t *testing.T) {
	rec := dvr.NewForTest(t, dvrFlags.Options()...)
	client := rec.Client()
	_ = client
}
```

You can then run tests in any mode:

```bash
go test ./...                                   # passthrough (default)
go test ./... -dvr.record -dvr.file testdata/api.dvr
go test ./... -dvr.replay -dvr.file testdata/api.dvr
```

### Credential Obfuscation & Custom Matching

* Use `dvr.WithObfuscator(dvr.BasicAuthObfuscator("user", "pass"))` to scrub
  HTTP Basic Auth credentials before requests are saved to disk.
* Use `dvr.WithMatcher(...)` to customize how incoming requests are matched
  against recorded entries during replay.

See the full API documentation on [pkg.go.dev](https://pkg.go.dev/github.com/liquidgecka/dvr).

## Contribution

Pull requests and issues are welcome via GitHub.

## License (Apache 2.0)

Copyright 2014 Orchestrate, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
