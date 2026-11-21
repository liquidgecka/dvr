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

package dvr_test

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/liquidgecka/dvr"
)

func Example_recordAndReplay() {
	dir, err := os.MkdirTemp("", "dvr-example-*")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	archivePath := filepath.Join(dir, "archive.dvr")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "hello from server")
	}))
	defer server.Close()

	// Step 1: Record an HTTP exchange to the archive file.
	rec, err := dvr.New(
		dvr.WithMode(dvr.ModeRecord),
		dvr.WithFile(archivePath),
		dvr.WithTransport(server.Client().Transport),
	)
	if err != nil {
		panic(err)
	}

	resp, err := rec.Client().Get(server.URL + "/greeting")
	if err != nil {
		panic(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err := rec.Close(); err != nil {
		panic(err)
	}

	// Shut down the server to demonstrate offline replay.
	server.Close()

	// Step 2: Replay the recorded exchange without network access.
	rep, err := dvr.New(
		dvr.WithMode(dvr.ModeReplay),
		dvr.WithFile(archivePath),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = rep.Close() }()

	replayedResp, err := rep.Client().Get(server.URL + "/greeting")
	if err != nil {
		panic(err)
	}
	body, err := io.ReadAll(replayedResp.Body)
	_ = replayedResp.Body.Close()
	if err != nil {
		panic(err)
	}

	fmt.Printf("%d %s", replayedResp.StatusCode, body)
	// Output:
	// 200 hello from server
}

func ExampleRegisterFlags() {
	fs := flag.NewFlagSet("example", flag.ContinueOnError)
	flags := dvr.RegisterFlags(fs)

	if err := fs.Parse([]string{"-dvr.replay", "-dvr.file", "testdata/custom.dvr"}); err != nil {
		panic(err)
	}

	fmt.Println(flags.Mode(), flags.File())
	// Output:
	// replay testdata/custom.dvr
}
